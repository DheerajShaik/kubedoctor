"""Check generated local reports in Chromium with all external requests denied."""
import argparse
import json
from pathlib import Path
import subprocess

from playwright.sync_api import expect, sync_playwright


def verify(args):
    dest = Path(args.output).resolve()
    dest.mkdir(parents=True, exist_ok=True)
    def load(page, path):
        document = Path(path).resolve()
        if args.document_preview:
            page.set_content(document.read_text(encoding="utf-8"))
        else:
            page.goto(document.as_uri())
    errors, remote = [], []
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(executable_path=args.chromium)
        context = browser.new_context(offline=True, viewport={"width": 1200, "height": 900})
        page = context.new_page()
        page.on("pageerror", lambda err: errors.append(str(err)))
        page.on("console", lambda msg: errors.append(msg.text) if msg.type == "error" else None)
        page.on("request", lambda req: remote.append(req.url) if not req.url.startswith("file:") else None)
        load(page, args.report)
        assert page.locator("#filters").is_visible(), "fixed filter script did not run"
        assert page.locator("[data-finding]").count() == 2
        assert page.locator("x-observation").count() == 0
        assert page.get_by_text("Deployment inventory was denied.").is_visible()
        for key, value, expected in [("severity", "warning", 1), ("namespace", "demo", 2),
                                    ("workload", "Deployment demo/shop", 2), ("resource", "Pod demo/shop-a", 1)]:
            page.locator(f"select[name={key}]").select_option(value)
            assert page.locator("[data-finding]:visible").count() == expected
            page.get_by_role("button", name="Clear filters").click()
        page.locator('a[href="#edge-0"]').first.click()
        expect(page.locator("#edge-0")).to_have_attribute("open", "")
        page.locator('a[href="#finding-0"]').first.click()
        expect(page.get_by_text("<x-observation>quoted & observed</x-observation>")).to_be_visible()
        page.locator('a[href="#summary"]').first.click()
        page.evaluate("window.scrollTo(0, 0)")
        page.screenshot(path=dest / "desktop.png")
        page.set_viewport_size({"width": 390, "height": 844})
        assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
        page.evaluate("window.scrollTo(0, 0)")
        page.screenshot(path=dest / "mobile.png")
        page.locator("select[name=severity]").select_option("info")
        page.emulate_media(media="print")
        assert not page.locator("#filters").is_visible()
        assert page.locator("[data-finding]:visible").count() == 2
        page.pdf(path=dest / "print.pdf", format="A4")
        paper = subprocess.check_output(["pdftotext", str(dest / "print.pdf"), "-"], text=True)
        for expected in ["quoted & observed", "Collection coverage", "Image pull is waiting", "CPU request unspecified"]:
            assert expected in paper, (expected, paper)
        static = browser.new_context(java_script_enabled=False, offline=True)
        static_page = static.new_page()
        load(static_page, args.report)
        assert not static_page.locator("#filters").is_visible()
        assert static_page.locator("[data-finding]:visible").count() == 2
        static_page.locator("#finding-0 summary").focus()
        static_page.keyboard.press("Enter")
        assert static_page.get_by_text("<x-observation>quoted & observed</x-observation>").is_visible()
        if args.example_dir:
            sample = Path(args.example_dir)
            result = json.loads((sample / "investigation.json").read_text(encoding="utf-8"))
            load(page, sample / "investigation.html")
            rows = page.locator("[data-finding]")
            assert rows.count() == len(result["report"]["findings"])
            for i, finding in enumerate(result["report"]["findings"]):
                content = rows.nth(i).text_content()
                for fact in [finding["rule_id"], finding["symptom"], finding["confidence"], finding["severity"]]:
                    assert fact in content, fact
                for evidence in finding["evidence"]:
                    assert evidence["field"] in content and evidence["value"] in content
            coverage = page.locator("#coverage").text_content()
            for source in result["report"]["sources"]:
                assert source["source"] in coverage and source["status"] in coverage
            assert page.locator("#relationships details.item").count() == len(result["relationships"])
            assert page.locator("#timeline li[id]").count() == len(result["timeline"])
        if args.comparison:
            page.emulate_media(media="screen")
            load(page, args.comparison)
            assert page.get_by_role("heading", name="Capture comparison").is_visible()
            assert page.get_by_role("heading", name="Resource changes").is_visible()
        browser.close()
    assert not remote, remote
    assert not errors, errors
    print("Offline Chromium passed filters, anchors, static content, keyboard, responsive layout, print, and network checks.")


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("report")
    p.add_argument("--comparison")
    p.add_argument("--example-dir", help="Compare an executable-generated example investigation.json and its HTML report")
    p.add_argument("--document-preview", action="store_true", help="Render local markup in memory for visual inspection on managed desktops that block file URLs")
    p.add_argument("--chromium", required=True)
    p.add_argument("--output", required=True)
    verify(p.parse_args())

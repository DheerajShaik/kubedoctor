# Offline investigation reports

Use `replay BUNDLE -o html --file investigation.html` and `compare BEFORE AFTER -o html --file comparison.html`. Each command creates one file and rejects an existing destination. Open the file directly. Use the section links to navigate, focus a disclosure and press Enter for supporting evidence, and select a finding from its investigation. The four finding filters act on severity, namespace, workload and resource. Comparison reports retain both the before and after evidence and coverage changes.

All evidence, coverage and uncertainty appear in the static document. A fixed script enables filters, and printing includes all findings and details. Each chart plots up to 96 time buckets from the requested window, from zero to the observed peak. A bucket spans its lowest and highest actual samples. Empty buckets remain blank. The adjacent statistics and request policy come from the shared analyzer.

Go's html/template escapes report data. The inline stylesheet and digest-authorized filtering script ship with the program. The document's content policy disables network connections and object embedding. An explicit HTTPS Grafana link is user-initiated navigation. Report generation fails if the HTML exceeds 32 MiB. Resource identifiers, ownership and the selected context can identify internal infrastructure. Share local reports with authorized recipients.

## Reproduce browser validation

The harness uses Chromium, Python Playwright and pdftotext. Use a fresh output directory.

```sh
mkdir -p /tmp/kubedoctor-browser
KUBEDOCTOR_HTML_TEST_OUTPUT=/tmp/kubedoctor-browser go test -count=1 -run '^TestRenderedReportAndBrowserFixture$' ./internal/htmlreport
python test/browser/offline.py /tmp/kubedoctor-browser/browser.html --example-dir docs/releases/v0.2.0/examples --comparison docs/releases/v0.2.0/examples/comparison.html --chromium "$(command -v chromium)" --output /tmp/kubedoctor-browser
```

The test loads local files in an offline browser, tracks network requests and script errors, exercises each filter and anchor, checks a narrow viewport, extracts print text, and repeats keyboard disclosure with JavaScript disabled. The seeded inert markup must remain visible as text.

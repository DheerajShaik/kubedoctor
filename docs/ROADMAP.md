# Roadmap

1. Add integration fixtures for additional Kubernetes minors and real-world CNI and DNS deployments. Extend supported environments when fixtures and compatibility evidence are available.
2. Add cluster-wide HTML reports over the versioned result schema. Keep export filters and sensitive data handling under the same policy as terminal and JSON output.
3. Add opt-in historical sizing with a real time series source such as Prometheus, a defined window, sufficient sample coverage, explicit aggregation and a documented sizing policy. Current Metrics API samples serve current-usage observations.
4. Add Grafana links or integration after the time series contract and credential boundary are established.
5. Expand network diagnosis and resource coverage. Document the boundary between configuration evidence and observed data-plane behavior.
6. Evaluate optional AI explanations and additional MCP transports after defining privacy controls, cost, and authentication. Keep the diagnostic engine deterministic.

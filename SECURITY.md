# Security

Report a suspected vulnerability privately to the repository owner. Use GitHub's private vulnerability reporting in the repository Security tab if it is enabled, or contact the owner through an existing private channel. Include the affected version, a synthetic reproduction and the expected security boundary. Omit kubeconfigs, credentials and real cluster data.

The engine issues only bounded read-only Kubernetes calls. Application logs and Secret objects are outside the supported collection surface. The MCP server uses the startup administrator's context and allowlist and enforces that scope in the adapter. Treat kubeconfigs and their authentication plugins as trusted configuration.

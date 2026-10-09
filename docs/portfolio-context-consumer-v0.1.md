# Portfolio Context Consumer v0.1

The runtime consumes Portfolio state through one sealed snapshot object.

`portfoliocontext.Parse(rawBytes)`:

- verifies the cross-language SHA-256 snapshot digest;
- rejects malformed snapshots;
- rejects `NON_EXECUTABLE` snapshots;
- exposes a compact `ReasoningView`;
- preserves human-final authority actions.

The package deliberately has no file or repository loader. Reading and binding source files belongs to the Portfolio snapshot generator. Runtime reasoning receives only the sealed bytes.

This prevents the reasoning layer from reopening arbitrary repository files to fill gaps after admission.

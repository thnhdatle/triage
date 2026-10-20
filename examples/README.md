# Examples

Run small scripts that show Triage's routing and privacy behaviour.

```sh
make examples   # run every example, compare stdout with expected.txt
```

Each `run.sh` starts and stops its own stack. Only one can run at a time (ports 8080, 8090, 9091).

- [decision-headers](decision-headers/README.md): routing headers and decision.
- [privacy-mask](privacy-mask/README.md): placeholders instead of PII.

# privacy-mask

See that a cloud target receives a placeholder instead of an e-mail address.

```sh
./run.sh   # starts the stack, sends the prompt, stops the stack
```

Needs Docker, curl, python3, free ports 8080, 8090, 9091 and the `make docker` images.

It prints what the cloud provider received: `[[EMAIL_1]]`, not the address. Expected output:
`expected.txt`. More: [docs/guides/privacy.md](../../docs/guides/privacy.md).

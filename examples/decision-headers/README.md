# decision-headers

See which target a short and a long prompt are routed to, and why.

```sh
./run.sh   # starts the stack, sends both prompts, stops the stack
```

Needs Docker, curl, python3, free ports 8080, 8090, 9091 and the `make docker` images.

It prints the routing headers and the decision. Short goes to local-small (weak), long to
cloud-strong (strong). Expected output: `expected.txt`.

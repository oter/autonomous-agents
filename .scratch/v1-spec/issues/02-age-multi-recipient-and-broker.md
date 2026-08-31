# age multi-recipient, and whether the broker already exists

Type: research
Status:
Blocked by:

## Question

[ADR-0001](../../../docs/adr/0001-runner-local-secret-broker.md) commits to a
Runner-local broker holding an age identity, with secrets encrypted to several
recipients. Establish that this is actually how `age` works, and find out
whether it needs building at all.

- Confirm multi-recipient encryption in `filippo.io/age`: encrypting one value
  to N recipients, decrypting with any one identity, and what the ciphertext
  costs in size when inlined into YAML.
- What is the Go API for loading an identity from a file and decrypting a value
  in memory? Any footguns around armored vs binary encoding in YAML?
- **Does this tool already exist?** Before we write a broker and a `dsecrets`,
  survey what covers the same ground: `sops` with age, `age-plugin-*`, `envchain`,
  `summon`, `chamber`, systemd credentials, Docker secrets. For each, say
  specifically why it does or does not give us "decrypt only these names, exec
  the child with them in its env, log the access".
- What is the minimum viable unix-socket broker in Go — how does it identify
  which Agent is calling, given the caller is in a container on the same host?
  That last part is the sharp bit and ticket 05 depends on the answer.

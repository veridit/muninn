# Security and privacy

muninn is local-first. Memories, embeddings and indexes stay on your machine.
The only network call it makes is to the embedding server you configure,
which defaults to a local Ollama at `http://127.0.0.1:11434`.

## No secrets in this repository

- Never commit real memories, memory exports, databases, `.env` files, API keys or tokens.
- Test fixtures under `testdata/` are synthetic only.
- `scripts/check-secrets.sh` runs as a pre-commit hook (`scripts/install-hooks.sh`) and in CI on every push. It has no bypass flag.
- muninn never reads harness credential files. Installers only touch hook configuration and keep a timestamped backup.

## Reporting

Report a vulnerability privately through GitHub's "Report a vulnerability" on this repository.

# Security

## Reporting a problem

Email **security@zharf.ai** with what you found, how to reproduce it, and which version
(`zharf version`) you used. Please do not open a public issue, and do not include anyone's API
key, including your own.

We will confirm we received the report, keep you posted while we work on it, and credit you in
the release notes if you would like.

## Supported versions

Fixes go into the latest release. Run `zharf update` to get it.

## What is in scope

- **Key storage.** `zharf login` keeps the API key in the macOS keychain, the Linux secret
  service (`secret-tool`), or `~/.config/zharf/credentials` with mode 0600. The key is never
  passed on a command line or written to logs.
- **`zharf serve`.** It spends the saved key's credit on behalf of whoever can reach it, so it:
  - listens on 127.0.0.1 unless told otherwise (`--host`);
  - refuses browser requests from other web sites (the `Origin` check);
  - on loopback, refuses requests addressed to other host names, which blocks DNS rebinding.
  A way around these checks is a security problem.
- **`zharf update` and the install scripts.** They download from `https://platform.zharf.ai/cli/`,
  check the size, check that the file is a program for this system, and run it once before
  installing it. Release assets on GitHub carry `SHA256SUMS`.
- **`zharf launch`.** It passes the key to the app it starts through an environment variable,
  and writes no file: each app's settings go in its arguments or environment for that run.

Problems with the platform itself (accounts, billing, the API at platform.zharf.ai) are also
welcome at the same address.

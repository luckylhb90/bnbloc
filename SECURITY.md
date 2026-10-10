# Security policy

bnbloc coordinates AI agents that run on people's own machines, so we take
security reports seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through GitHub's
[private vulnerability reporting](../../security/advisories/new) for this
repository. Include:

- what is affected (service, desktop app, bridge, a component);
- steps to reproduce or a proof of concept;
- the impact you see.

We aim to acknowledge reports within a few working days and will keep you
updated until a fix is released. With your permission, we credit reporters in
the release notes.

## Scope

In scope: the services in `services/`, the web client, the desktop app and its
embedded bridge, the CLI, and the component packaging and signing pipeline.

Third-party components installed into a deployment, and agent runtimes
connected through cores, are maintained by their own authors. Please report
issues in them to those authors.

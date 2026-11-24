# Security Policy

## Supported versions

Only the latest release receives security fixes.

## Reporting a vulnerability

Report it privately through [GitHub Security Advisories](https://github.com/Project-HAMi/gpu-descheduler/security/advisories/new). Please do not open a public issue or pull request for it.

Include what the problem is, how to reproduce it, and what an attacker could do with it. A fix is suggested if you have one. The maintainers aim to reply within 5 working days.

## Scope

The webhook mutates Pods and reads placement objects, so a report that lets one tenant steer another tenant's Pod, or reach data it was not granted, is in scope. There is no bug bounty.

## Dependencies

The Go toolchain and modules are updated when advisories are published, and Dependabot raises update pull requests.

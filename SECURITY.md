# Security

## Reporting a vulnerability

**Use GitHub's private vulnerability reporting:** on the repository, *Security
→ Report a vulnerability*. That opens a thread only you and the maintainers
can read.

If that is not available to you, write to the address on the instance you
found it on, or open a discussion saying only that you have something to
report and how to reach you — without the detail.

**Please do not open a public issue or pull request for a vulnerability.** A
pull request discloses the finding, the fix and the affected version all at
once, to everybody, before a single instance has been upgraded. That has
already happened here once, and it was the project's fault for having no
other route: this file is the remedy.

What helps, in rough order of usefulness:

- what the attacker gets, rather than what the code does;
- how you found it — by reading, or with a tool, and which tool;
- an instance version, or a commit;
- whether it has been disclosed anywhere else.

What you will get back: an acknowledgement, and a plain answer about whether
it is being fixed and roughly when. If a finding turns out not to be one, you
will be told why rather than left waiting.

## What is in scope

This is a latency monitoring tool that publishes measurements. The things
worth attacking are:

| | |
|---|---|
| **Private measurements** | a target marked not public, or hidden behind a share link, must not reach a visitor — through the API, through a shared cache, or through the public pages |
| **The probe** | it holds a raw socket. Making it measure something it was not asked to, or anything on the machine it runs on, is in scope |
| **The federation** | a peer is semi-trusted. Anything a peer can make an instance do beyond what the protocol allows — measure a private address, publish its text, impersonate another peer — is in scope |
| **The release path** | the workflows sign binaries that instances install by themselves. Anything that puts code or a version number through them is the most serious class |
| **The back-office** | authentication, session handling, role boundaries. An editor reaching what only an admin may do counts |

Out of scope: the measurements themselves being wrong or unflattering;
anything that requires an operator to attack their own instance with
credentials they already hold; denial of service by volume against a public
page that is meant to be read by anyone.

## Contributions on the security path

Findings from anyone are welcome and will be credited by name in the
changelog. For `api.go`, `auth.go`, `notify.go`, `doublecheck.go`, `fedapi.go`,
`fedsafe.go`, `share.go` and the files under `.github/workflows/`, a first
contribution is taken as a **finding and rewritten**, rather than merged as a
patch.

That is not a judgement about anyone's code. This repository builds a signed
binary that instances install within hours of a release, so a patch merged
here reaches machines whose operators never saw the pull request. "I read it
carefully and found nothing" is a weaker statement than it sounds, and it is
the only one a maintainer can make about a diff from an account with no
history. Rewriting costs a day; being wrong costs other people's servers.

What moves a contributor off that footing is history: contributions that
landed, and signed commits, so a diff arrives from a key rather than from a
name.

## Supported versions

The newest release. Instances with automatic updates install it by
themselves; see [DEPLOY.md](DEPLOY.md). There are no backports — fixes go
into the next version, which is usually out within days.

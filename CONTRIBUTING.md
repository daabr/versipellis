<!-- omit in toc -->
# Contributing to Versipellis

First off, thanks for taking the time to contribute! ❤️

All types of contributions are encouraged and valued. See the [Table of Contents](#table-of-contents) for different ways to help and details about how this project handles them. Please make sure to read the relevant section before making your contribution. It will make it a lot easier for us maintainers and smooth out the experience for all involved. The community looks forward to your contributions. 🎉

> And if you like this project, but just don't have time to contribute, that's fine. There are other easy ways to support this project and show your appreciation:
>
> - Star the project
> - Write about it
> - Tell your friends and colleagues

<!-- omit in toc -->
## Table of Contents

- [Asking Questions](#asking-questions)
- [Reporting Issues](#reporting-issues)
- [Suggesting Enhancements](#suggesting-enhancements)
- [I Want To Contribute](#i-want-to-contribute)
  - [Improving Documentation](#improving-documentation)
  - [Development Setup](#development-setup)
  - [Building and Testing](#building-and-testing)
  - [Code Quality](#code-quality)
  - [Style Guide](#style-guide)
  - [Submitting a Pull Request](#submitting-a-pull-request)

## Asking Questions

First, we assume that you've read the [documentation](https://daabr.github.io/versipellis/).

Also, it's best to search for [existing issues](https://github.com/daabr/versipellis/issues) that might help you. In case you find a suitable issue and you have additional clarifications/requests/details to share, you can add them in the existing issue. It is also advisable to search the internet for answers first.

If you still want to ask a question, we recommend the following:

- [Open a new issue](https://github.com/daabr/versipellis/issues/new/choose)
- Explain what you're trying to achieve, what you're observing, and what needs clarification
- Specify project and environment details (Versipellis, the platform/architecture/OS, connected services, etc.) - depending on what seems relevant

We will then take care of the issue as soon as possible.

## Reporting Issues

In addition to the above...

<!-- omit in toc -->
### Before Submitting a Bug Report

A good bug report shouldn't leave others needing to chase you up for more information. Therefore, we ask you to investigate carefully, collect information and describe the issue in detail in your report. Please complete the following steps in advance to help us fix any potential bug as fast as possible.

- Make sure that you're using the latest version
- Determine if your issue is a bug in Versipellis or an error on your side, e.g., using incompatible environment components/versions
- If you're looking for support, you might want to check [this section](#asking-questions)
- To see if others have experienced (and potentially already solved) the same issue or similar ones, check if there isn't already an [existing issue](https://github.com/daabr/versipellis/issues)
- Also make sure to search the internet to see if users outside of the GitHub community have discussed any similar issues

Collect information about the bug:

- Logs with relevant details, warnings and errors, and stack traces if there are any
- OS, platform, and version (Windows, Linux, macOS, x86, ARM)
- Input and output data, if relevant and possible
- Can you reliably reproduce the issue?

<!-- omit in toc -->
### How Do I Submit a Good Bug Report?

> [!CAUTION]
> Never report security-related issues, vulnerabilities or bugs including sensitive information to the public issue tracker, or elsewhere in public.
>
> Instead, sensitive bugs must be [reported privately to the project's maintainers](https://github.com/daabr/versipellis/security/advisories/new) - see [SECURITY.md](./SECURITY.md) for details.

We use GitHub issues to track bugs and errors. If you run into an issue with the project:

- [Open a new issue](https://github.com/daabr/versipellis/issues/new/choose)
- Explain what you're trying to achieve, and what you're expecting, in comparison to the actual behavior
- Please provide as much context as possible and describe the *reproduction steps* that someone else can follow to recreate the issue on their own
- For good bug reports you should isolate the problem and create a minimalistic test case, if possible
- Provide the information you collected in the previous section

Once it's filed:

- We will label and prioritize the issue accordingly
- We will try to reproduce the issue with your provided steps, or ask for additional information

## Suggesting Enhancements

This section guides you through submitting an enhancement suggestion for Versipellis, **including completely new features and minor improvements to existing functionality**. Following these guidelines will help maintainers and the community to understand your suggestion and find related suggestions.

<!-- omit in toc -->
### Before Submitting an Enhancement

- Make sure that you're using the latest version
- Read the [documentation](https://daabr.github.io/versipellis/) carefully and find out if the functionality is already covered, maybe as a configurable option
- Perform a [search](https://github.com/daabr/versipellis/issues) to see if the enhancement has already been suggested
  - If it has, add a comment to the existing issue instead of opening a new one

Consider whether your idea fits with the scope and aims of the project. It's up to you to make a strong case to convince the project's developers of the merits of this feature. Keep in mind that we want features that will be useful to the majority of our users and not just a small subset.

<!-- omit in toc -->
### How Do I Submit a Good Enhancement Suggestion?

Enhancement suggestions are tracked as [GitHub issues](https://github.com/daabr/versipellis/issues).

- Use a **clear and descriptive title** for the issue to identify the suggestion
- Provide a **step-by-step description of the suggested enhancement** in as many details as possible
- **Describe the current behavior** and **explain which behavior you expected to see instead** and why
- At this point you can also tell which alternatives do not work for you, and possibly why
- **Explain why this enhancement would be useful** to Versipellis users, you may also want to point out the other projects that solved it better and which could serve as inspiration

## I Want To Contribute

> ### Legal Notice
>
> When contributing to this project, you [certify](https://developercertificate.org/) that you have authored 100% of the content, that you have the necessary rights to the content, and that your contributions are licensed under the project's [Apache License 2.0](./LICENSE).

### Improving Documentation

Contributions to documentation are always appreciated:

- Documentation files are located under the [`docs/`](./docs/) directory and in [`README.md`](./README.md)
- Fixes, improved explanations, and new examples or tutorials are welcome!

### Development Setup

To build and test locally, you need at least these tools:

- Git
- [Go](https://go.dev/dl/) (version 1.27 or newer)
- [Golangci-lint](https://golangci-lint.run/docs/welcome/) (latest version)
- [Editor / IDE](https://go.dev/wiki/IDEsAndTextEditorPlugins)

### Building and Testing

Build with pure Go (standard):

```shell
CGO_ENABLED=0 go build ./cmd/versi
```

Build with ODBC and Oracle Database support:

```shell
CGO_ENABLED=1 go build -tags=odbc ./cmd/versi
```

Run the test suite across all packages:

```shell
go test ./pkg/...
```

### Code Quality

Before opening a pull request, ensure your code is cleanly formatted and passes static analysis:

```shell
golangci-lint fmt && golangci-lint run
```

Also, run the tests with the data race detector and coverage profile ([matches CI](./.github/workflows/ci.yml#L28)):

```shell
go test -race -covermode=atomic -coverprofile=coverage.out ./pkg/...
```

Finally, identify and address test coverage regressions:

```shell
go tool cover -html=coverage.out
```

### Style Guide

<!-- omit in toc -->
#### Code Conventions

- Follow standard Go best practices outlined in [Effective Go](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- Keep functions and methods modular, with clear logic and error handling
- Avoid introducing **unnecessary** external dependencies
- Commenting style: see [Go doc comments](https://go.dev/doc/comment)
- Testing: see [Go test comments](https://go.dev/wiki/TestComments) and [table-driven tests](https://go.dev/wiki/TableDrivenTests)

### Submitting a Pull Request

1. [Fork the repository](https://github.com/daabr/versipellis/fork) and create a new branch from `main`
2. Write clean, idiomatic, maintainable code with unit tests covering new or modified functionality
3. [Run the test suite and the linter](#code-quality) to make sure all checks pass
4. Make focused, atomic commits with clear, concise descriptions
   - Attention: [all commits must be signed](https://docs.github.com/en/authentication/managing-commit-signature-verification/about-commit-signature-verification) in order to be merged!
   - Tip: to fix an unsigned commit, rebase it to include a verified signature, then force-push the rewritten commit to your branch
5. Push your branch to your fork and submit a Pull Request against the `main` branch
   - Title: follow the [Conventional Commits](https://www.conventionalcommits.org/) specification
   - Describe the purpose, changes, and testing strategy of your PR
   - Reference any relevant issues (e.g., `Fixes #123` or `Closes #456`)
6. Review expectations: read [Google's CL author's guide to getting through code review](https://google.github.io/eng-practices/review/developer/)

<!-- omit in toc -->
## You Can Stop Reading Now

FYI - for educational purposes only, just in case you're interested:

- [Google's Go style guide](https://google.github.io/styleguide/go/)
- [Uber's Go style guide](https://github.com/uber-go/guide/blob/master/style.md)
- Go Wiki deep-dives: [concurrency](https://go.dev/wiki/LearnConcurrency), [error handling](https://go.dev/wiki/LearnErrorHandling), [servers](https://go.dev/wiki/LearnServerProgramming)

<!-- omit in toc -->
## Attribution

This guide is based on the [contributing.md](https://contributing.md/generator)!


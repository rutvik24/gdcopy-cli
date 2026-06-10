# Contributing to gdcopy

Thank you for your interest in contributing! We welcome help in making this tool faster, more robust, and easier to use.

## How to Contribute

### 1. Reporting Bugs & Suggesting Features

- Search the existing issues to ensure your bug or suggestion hasn't already been reported.
- If it's a new issue, use the **Bug Report** or **Feature Request** templates to file a clear issue.
- Provide steps to reproduce the issue, what you expected, and what actually occurred.

### 2. Pull Requests

1. **Fork the Repository** and create your branch from `main`:
   ```bash
   git checkout -b feature/my-amazing-feature
   ```
2. **Make your changes**. Ensure that:
   - Your code is properly formatted: `go fmt ./...`.
   - Your code passes all tests: `go test -v`.
   - You add tests for new functionality if applicable.
3. **Commit your changes**: Write clear, descriptive commit messages.
4. **Push your branch** to your fork.
5. **Open a Pull Request** using the PR template provided.

### 3. Development Setup

- Ensure you have Go installed (v1.18+).
- Set up a Google Cloud Console desktop client credential (as outlined in the [README.md](README.md)) and copy it locally as `credentials.json` to run/test your modifications. Note: **Do not commit your `credentials.json` or `token.json` files!**

### 4. Code of Conduct

Please note that this project is released with a Contributor Code of Conduct (see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)). By participating in this project, you agree to abide by its terms.

# Contributing to Docker AmneziaWG

Thank you for your interest in contributing to this project! This document provides guidelines for contributing.

## How to Contribute

### Reporting Issues

1. Check if the issue already exists in the [Issues](https://github.com/lqflqf/docker-amneziawg/issues) section
2. If not, create a new issue with:
   - Clear description of the problem
   - Steps to reproduce
   - Expected vs actual behavior
   - Docker version and host OS
   - Relevant logs or error messages

### Suggesting Enhancements

1. Open an issue with the "enhancement" label
2. Describe the proposed feature and its benefits
3. Include examples of how it would be used

### Pull Requests

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/your-feature-name`
3. Make your changes
4. Test your changes thoroughly
5. Update documentation if needed
6. Commit with clear, descriptive messages
7. Push to your fork and submit a pull request

## Development Guidelines

### Code Style

- Follow existing code style and conventions
- Use clear, descriptive variable and function names
- Add comments for complex logic
- Keep Dockerfile efficient and secure

### Testing

- Test with different AmneziaWG configurations
- Verify both server and client setups
- Test with Docker and Docker Compose
- Ensure the container starts and stops gracefully

### Documentation

- Update README.md if adding new features
- Update configuration examples if needed
- Add inline documentation for complex scripts

## Project Structure

```
├── Dockerfile          # Multi-stage build configuration
├── docker-compose.yml  # Compose setup example
├── entrypoint.sh       # Container startup script
├── awg0.conf.example   # Configuration template
├── .github/workflows/  # CI/CD automation
└── README.md          # Project documentation
```

## Building and Testing Locally

```bash
# Build the image
docker build -t amneziawg-test .

# Test with a configuration
cp awg0.conf.example awg0.conf
# Edit awg0.conf with your settings
docker run --rm -it --cap-add NET_ADMIN --device /dev/net/tun \
  -v $(pwd)/awg0.conf:/etc/wireguard/awg0.conf \
  amneziawg-test awg0
```

## Commit Message Format

Use clear, imperative commit messages:

- `feat: add support for custom interface names`
- `fix: resolve startup script permissions issue`
- `docs: update configuration examples`
- `chore: update base image to Alpine 3.19`

## Questions?

If you have questions about contributing, feel free to:
- Open an issue with the "question" label
- Start a discussion in the repository

Thank you for contributing! 🚀

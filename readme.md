# Network Checks

This tool probes the availability of services using HTTP requests and system ping commands.

## Usage
1. Define the list of services to check in `checks.yml`.
2. Run the script with: `go run .`.

## Configuration
Services are defined in the `checks.yml` file using the following format:

```yaml
  - name: google.com
    type: http
    dest: https://google.com
    repeat: 30s
  - name: seznam.cz
    type: icmp
    dest: seznam.cz
    repeat: 10s
```

# Compare Batches Script

This directory contains the `compare_batches.sh` script for comparing batch data between two domains.

## Usage

To use the `compare_batches.sh` script:

1. Make the script executable:

   ```bash
   chmod +x compare_batches.sh
   ```

2. Run it with two domain names:

   ```bash
   ./compare_batches.sh localhost:1633 other_domain
   ```

## What the script does

The `compare_batches.sh` script:

- Checks for correct number of arguments (requires exactly 2 domain names)
- Uses `curl -s` for silent mode to fetch `/batches` endpoint from both domains
- Pipes output through `jq` to format JSON
- Saves results to `1.txt` and `2.txt`
- Uses `diff --side-by-side --suppress-common-lines` to show only differing lines in a side-by-side format
  - `--side-by-side` displays the differing lines from both files next to each other
  - `--suppress-common-lines` ensures only differing lines are shown
- Reports if differences were found or not

## Output

The script will display differences between the two batch endpoints side by side, or report "No differences found" if the responses are identical.

## Build & Push

To build and push the Docker image for this script, you can use the following commands:

```bash
docker build -t your_dockerhub_username/compare_batches.sh:latest .
docker push your_dockerhub_username/compare_batches.sh:latest
```

#!/bin/bash

# Check if two domain arguments are provided
if [ $# -ne 2 ]; then
    echo "Usage: $0 <domain1> <domain2>"
    exit 1
fi

DOMAIN1=$1
DOMAIN2=$2
FILE1="1.txt"
FILE2="2.txt"

# Perform curl requests in parallel
curl -s "http://${DOMAIN1}/batches" | jq > "$FILE1" &
PID1=$!
curl -s "http://${DOMAIN2}/batches" | jq > "$FILE2" &
PID2=$!

# Wait for both curl commands to complete
wait $PID1
if [ $? -ne 0 ]; then
    echo "Error fetching data from ${DOMAIN1}"
    exit 1
fi

wait $PID2
if [ $? -ne 0 ]; then
    echo "Error fetching data from ${DOMAIN2}"
    exit 1
fi

# Compare the files and show differences side by side
echo "Differences between ${FILE1} and ${FILE2} (side by side):"
diff --side-by-side --suppress-common-lines "$FILE1" "$FILE2"

# Check if there were any differences
if [ $? -eq 0 ]; then
    echo "No differences found."
else
    echo "Differences found (see above)."
fi

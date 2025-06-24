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
TMP1="tmp1.json"
TMP2="tmp2.json"

# Fetch both responses in parallel
curl -s "http://${DOMAIN1}/batches" > "$TMP1" &
PID1=$!
curl -s "http://${DOMAIN2}/batches" > "$TMP2" &
PID2=$!

# Wait and check each curl
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

# Get batch counts
COUNT1=$(jq '.batches | length' "$TMP1" 2>/dev/null)
COUNT2=$(jq '.batches | length' "$TMP2" 2>/dev/null)

# Validate batch presence
if [ -z "$COUNT1" ] || [ "$COUNT1" -eq 0 ]; then
    echo "No batches found in response from ${DOMAIN1}"
    exit 1
fi

if [ -z "$COUNT2" ] || [ "$COUNT2" -eq 0 ]; then
    echo "No batches found in response from ${DOMAIN2}"
    exit 1
fi

# Print batch counts
echo "Batch count from ${DOMAIN1}: $COUNT1"
echo "Batch count from ${DOMAIN2}: $COUNT2"

# Strip batchTTL and save for comparison
jq '.batches | map(del(.batchTTL))' "$TMP1" > "$FILE1"
jq '.batches | map(del(.batchTTL))' "$TMP2" > "$FILE2"

# Compare the files and show differences side by side
echo "Differences between ${FILE1} and ${FILE2} (side by side):"
if diff --side-by-side --suppress-common-lines "$FILE1" "$FILE2"; then
    echo "No differences found."
    rm -f "$TMP1" "$TMP2"
else
    echo "Differences found (see above)."
fi

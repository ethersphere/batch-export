FROM alpine:3.20

RUN apk add --no-cache bash curl jq diffutils

COPY compare_batches.sh /compare_batches.sh
RUN chmod +x /compare_batches.sh

ENTRYPOINT ["/compare_batches.sh"]

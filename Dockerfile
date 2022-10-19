FROM alpine:3.15.4

RUN apk add gcompat

COPY test-adapter /usr/local/bin/adapter

EXPOSE 8080

CMD ["/usr/local/bin/adapter", "--logtostderr=true"]

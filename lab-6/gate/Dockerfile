FROM python:3.14-alpine

RUN addgroup -S -g 10001 gate && adduser -S -D -H -u 10001 -G gate gate
WORKDIR /app
COPY gate.py /app/gate.py
USER 10001:10001
ENTRYPOINT ["python", "/app/gate.py"]

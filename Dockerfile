FROM ubuntu:latest
LABEL authors="aiquen"

RUN groupadd -g 142 bot
RUN useradd -u 142 -g bot -m -d /app -r -s /sbin/nologin bot

RUN apt update -y && apt install -y libolm3 ca-certificates

RUN update-ca-certificates --fresh

COPY --chown=142:142 builds/init-bot /app/

WORKDIR /app
USER bot

CMD ["./init-bot"]
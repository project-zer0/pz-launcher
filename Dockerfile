FROM php:alpine

# Install DockerCompose
RUN apk update && \
    apk add --no-cache docker-cli docker-compose; \
    docker-php-ext-install sockets
FROM python:3.13-slim@sha256:9d2e5553305c7c7b0097999bb17187c69b921ccd6bc9d40e4bb5ebe652c00285
# Apply available distribution fixes; offline submissions use no package installer.
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*
RUN python3 -m pip uninstall -y pip
RUN mkdir /work && chmod 1777 /work
WORKDIR /work
USER 65534:65534
CMD ["sleep", "infinity"]

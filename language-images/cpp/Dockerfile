FROM gcc:14@sha256:3ae7320d7dd41f446a48930e1edf4e7a41c7be5ae43a5ddbf017c53fe6495738
# Apply distribution security fixes, then certify and run the resulting immutable image ID.
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*
RUN mkdir /work && chmod 1777 /work
WORKDIR /work
USER 65534:65534
CMD ["sleep", "infinity"]

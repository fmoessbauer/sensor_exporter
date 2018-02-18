FROM ubuntu:16.04

MAINTAINER Felix Moessbauer

COPY sensor_exporter /
RUN chmod 755 /sensor_exporter

RUN apt update && apt install --yes nut-client

EXPOSE 9091
ENTRYPOINT ["./sensor_exporter"]
CMD ["-h"]


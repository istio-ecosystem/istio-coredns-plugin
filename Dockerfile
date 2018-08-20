FROM tutum/curl
RUN apt-get update && apt-get install -y dnsutils
#ADD coredns /usr/local/bin/
#ADD Corefile /usr/local/bin/
#ADD run.sh /usr/local/bin/

ADD plugin /usr/local/bin/
CMD /usr/local/bin/plugin

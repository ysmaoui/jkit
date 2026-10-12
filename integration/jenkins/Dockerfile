FROM jenkins/jenkins:2.541.2-lts-jdk21

ENV JAVA_OPTS="-Djenkins.install.runSetupWizard=false"
ENV CASC_JENKINS_CONFIG=/var/jenkins_home/casc.yml

COPY --chown=jenkins:jenkins plugins.txt /usr/share/jenkins/ref/plugins.txt
# jenkins-plugin-cli is Java and ignores *_PROXY env vars; translate them to -D flags.
# Proxy credentials are dropped (-D props can't carry proxy auth).
# *_PROXY build args are Docker-predefined: visible to RUN, kept out of image history.
RUN set -eu; \
    proxy_flags() { \
      case "$1" in https://*) port=443 ;; *) port=80 ;; esac; \
      hostport="${1#*://}"; hostport="${hostport##*@}"; hostport="${hostport%%/*}"; \
      host="${hostport%:*}"; [ "$host" != "$hostport" ] && port="${hostport##*:}"; \
      echo "-D$2.proxyHost=$host -D$2.proxyPort=$port"; }; \
    https_url="${HTTPS_PROXY:-${https_proxy:-}}"; http_url="${HTTP_PROXY:-${http_proxy:-}}"; \
    no_proxy_list="${NO_PROXY:-${no_proxy:-}}"; proxy_opts=""; \
    [ -z "$https_url" ] || proxy_opts="$proxy_opts $(proxy_flags "$https_url" https)"; \
    [ -z "$http_url" ] || proxy_opts="$proxy_opts $(proxy_flags "$http_url" http)"; \
    [ -z "$no_proxy_list" ] || proxy_opts="$proxy_opts -Dhttp.nonProxyHosts=$(echo "$no_proxy_list" | sed 's/ //g; s/,/|/g; s/\(^\||\)\./\1*./g')"; \
    JAVA_OPTS="$JAVA_OPTS$proxy_opts" jenkins-plugin-cli --plugin-file /usr/share/jenkins/ref/plugins.txt

COPY --chown=jenkins:jenkins casc.yml /var/jenkins_home/casc.yml
COPY --chown=jenkins:jenkins init.groovy.d/ /usr/share/jenkins/ref/init.groovy.d/

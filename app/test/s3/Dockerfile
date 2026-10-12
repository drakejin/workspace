# S3 e2e 전용 OpenSearch — repository-s3 플러그인과 LocalStack 용 더미 자격증명(test/test)을 넣는다
FROM opensearchproject/opensearch:2.19.2
RUN /usr/share/opensearch/bin/opensearch-plugin install --batch repository-s3 \
 && (test -f /usr/share/opensearch/config/opensearch.keystore || /usr/share/opensearch/bin/opensearch-keystore create) \
 && echo test | /usr/share/opensearch/bin/opensearch-keystore add --stdin --force s3.client.default.access_key \
 && echo test | /usr/share/opensearch/bin/opensearch-keystore add --stdin --force s3.client.default.secret_key

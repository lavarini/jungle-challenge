#!/bin/bash
# Provisions queues, DLQ redrive, the SNS FIFO topic and its audit subscriber.
set -euo pipefail

DLQ_URL=$(awslocal sqs create-queue --queue-name wager-transactions-dlq.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false \
  --query QueueUrl --output text)
DLQ_ARN=$(awslocal sqs get-queue-attributes --queue-url "$DLQ_URL" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)

cat > /tmp/wager-transactions-attributes.json <<EOF
{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "VisibilityTimeout": "30",
  "ReceiveMessageWaitTimeSeconds": "20",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"${DLQ_ARN}\",\"maxReceiveCount\":\"20\"}"
}
EOF
awslocal sqs create-queue --queue-name wager-transactions.fifo \
  --attributes file:///tmp/wager-transactions-attributes.json > /dev/null

TOPIC_ARN=$(awslocal sns create-topic --name wallet-events.fifo \
  --attributes FifoTopic=true,ContentBasedDeduplication=false \
  --query TopicArn --output text)

AUDIT_URL=$(awslocal sqs create-queue --queue-name wallet-events-audit.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false \
  --query QueueUrl --output text)
AUDIT_ARN=$(awslocal sqs get-queue-attributes --queue-url "$AUDIT_URL" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)

awslocal sns subscribe --topic-arn "$TOPIC_ARN" --protocol sqs \
  --notification-endpoint "$AUDIT_ARN" \
  --attributes RawMessageDelivery=true > /dev/null

echo "wagering-resources-ready"

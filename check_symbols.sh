#!/bin/bash
for s in InjectIdentitySystemIntoResponsesInput ValidatePublicModel BuildResolvedModel ContextKeyPublicModel ContextKeyUpstreamModel injectIdentitySystemIntoAnthropicRequest BuildResolvedModel; do
  printf '%s=' "$s"
  strings /opt/sub2api/sub2api | grep -c "$s"
done
# Versipellis Roadmap

This is the high-level plan for features that are expected to be released within the next 12 months:

## 0-3 Months

1. Configurable HTTP authentication & authorization

   - None
   - Basic (RFC 7617)
   - Static key/token
   - OAuth 2
     - Interactive authorization
     - Client credentials
     - Refresh
     - JWT (RFC 7523)
     - Workload Identity Federation (WIF)
     - Session cookie (RFC 6265)

2. Respect rate-limiting responses from HTTP servers

3. Stream DB rows instead of current buffering, and batch at the HTTP sender side

4. Data collection from MongoDB (+ general support for BSON)

5. Configurable usage of the DLQ sender as a fallback after sender failures

## 3-6 Months

1. Dynamic evaluation of configuration expressions (e.g., secrets management & data checkpointing)

2. Multi-step data collection (using one collector's results to populate another collector's query)

3. Additional HTTP content encoding options (see issues #39, #40, #41)

   - Automatic decoding of incoming client requests and server responses
   - Configurable encoding of outgoing collector/sender requests

4. Additional auth options, e.g., AWS, Azure, GCP, CyberArk

## 6-12 Months

1. Additional types of input sources and output destinations

   - OTLP and other observability data
   - More NoSQL data stores
   - WebSocket (RFC 6455)
   - Messaging queues
   - Cloud service providers

2. Support for additional content types in HTTP senders

   - Tier 1: Apache Parquet, Protocol Buffers, Apache Avro
   - Tier 2: Apache ORC, MessagePack, CBOR (RFC 8949)

3. Configurable tuning of connection pools

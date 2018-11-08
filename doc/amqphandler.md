# AMQP Handler

`amqphandler` handles communication protocol with IoT gateway. It has two part:
* `sender` - send messages to the Gateway
* `receiver` - receive messages from the Gateway and path them to appropriate AOS packages

On connect,`amqphandler` sends service discovery request to Service Discovery server. As response it receives IoT Gateway connection parameters. Then `amqphandler` connects to the Gateway with received parameters and initialize `sender` and `receiver` part.

Connections between incoming/outgoing messages and AOS packages is done in the main loop.

# VIS Client

VIS client communicates with VIS server to provide VIS data to SM. At the start VIS client gets VIN and user claims from VIS server. These info is used to authenticate with AOS cloud. Also VIS client subscribes on user claims change notification in order to reconnect to AOS cloud in case user claims are changed runtime.

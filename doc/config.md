# AOS Service Manager configuration file

The configuration file has JSON format. Following is JSON schema:

```json
{
    "definitions": {
        "alertRule": {
            "description": "Alert rule",
            "type": "object",
            "required": [
                "minTimeout",
                "minThreshold",
                "maxThreshold"
            ],
            "properties": {
                "minTimeout": {
                    "description": "Minimal timeout",
                    "type": "string"
                },
                "minThreshold": {
                    "description": "Minimal threshold",
                    "type": "integer",
                    "minimum": 0
                },
                "maxThreshold": {
                    "description": "Maximal threshold",
                    "type": "integer",
                    "minimum": 0
                }
            }
        }
    },
    "description": "AOS Service Manager Configuration file",
    "type": "object",
    "required": [
        "fcrypt",
        "serviceDiscovery",
        "workingDir"
    ],
    "properties": {
        "fcrypt": {
            "description": "AOS Service manager crypt configuration",
            "type": "object",
            "required": [
                "CACert",
                "ClientCert",
                "ClientKey",
                "OfflinePrivKey",
                "OfflineCert"
            ],
            "properties": {
                "CACert": {
                    "description": "CA certificate",
                    "type": "string"
                },
                "ClientCert": {
                    "type": "string",
                    "description": "Client certificate"
                },
                "ClientKey": {
                    "type": "string",
                    "description": "Client key"
                },
                "OfflinePrivKey": {
                    "type": "string",
                    "description": "Offline private key"
                },
                "OfflineCert": {
                    "type": "string",
                    "description": "Offline certificate"
                }
            }
        },
        "serviceDiscovery": {
            "description": "Address of service discovery server",
            "type": "string"
        },
        "workingDir": {
            "description": "Directory where AOS data will be stored",
            "type": "string"
        },
        "defaultServiceTTLDays": {
            "description": "Specifies how long  to keep service and its data when it is not used",
            "type": "integer",
            "minimum": 0,
            "default": 30
        },
        "monitoring": {
            "description": "Resource monitoring parameters",
            "type": "object",
            "properties": {
                "disabled": {
                    "description": "Enable/disable monitoring",
                    "type": "boolean",
                    "default": false
                },
                "sendPeriod": {
                    "description": "Send monitoring data period in ISO 8601 format: 01:30:12",
                    "type": "string",
                    "default": "00:01:00"
                },
                "pollPeriod": {
                    "description": "Get and analyze monitoring data period in ISO 8601 format: 01:30:12",
                    "type": "string",
                    "default": "00:00:10"
                },
                "maxOfflineMessages": {
                    "description": "Indicates how many monitoring messages to keep when vehicle in offline",
                    "type": "integer",
                    "minimum": 0,
                    "default": 25
                },
                "netnsBridgeIP": {
                    "description": "Specifies netns bridge subnet to count as local traffic. Should be set if netns bridge subnet is changed",
                    "type": "string",
                    "default": "172.19.0.0/16"
                },
                "ram": {
                    "description": "RAM alert rules",
                    "$ref": "#/definitions/alertRule"
                },
                "cpu": {
                    "description": "CPU alert rules",
                    "$ref": "#/definitions/alertRule"
                },
                "usedDisk": {
                    "description": "Disk usage alert rules",
                    "$ref": "#/definitions/alertRule"
                },
                "inTraffic": {
                    "description": "IN traffic alert rules",
                    "$ref": "#/definitions/alertRule"
                },
                "outTraffic": {
                    "description": "OUT traffic alert rules",
                    "$ref": "#/definitions/alertRule"
                }
            }
        },
        "logging": {
            "description": "Service logging parameters",
            "type": "object",
            "properties": {
                "maxPartSize": {
                    "description": "Indicates maximum size of logging part in bytes",
                    "type": "integer",
                    "minimum": 0,
                    "default": 524288
                },
                "maxPartCount": {
                    "description": "Indicates maximum part count",
                    "type": "integer",
                    "minimum": 0,
                    "default": 20
                }
            }
        },
        "alerts": {
            "description": "Alerts parameters",
            "type": "object",
            "properties": {
                "disabled": {
                    "description": "Enable/disable sending alerts",
                    "type": "boolean",
                    "default": false
                },
                "sendPeriod": {
                    "description": "Send alerts minimum period in ISO 8601 format: 01:30:12",
                    "type": "string",
                    "default": "00:00:10"
                },
                "maxMessageSize": {
                    "description": "Indicates maximum size of one alerts message",
                    "type": "integer",
                    "minimum": 0,
                    "default": 65536
                },
                "maxOfflineMessages": {
                    "description": "Indicates how many alert messages to keep when vehicle in offline",
                    "type": "integer",
                    "minimum": 0,
                    "default": 25
                }
            }
        },
        "identifier": {
            "description": "Identifier parameters",
            "type": "object",
            "required": [
                "module"
            ]
        }
    }
}
```
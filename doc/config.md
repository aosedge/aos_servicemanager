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
        "cmServer",
        "serviceDiscovery",
        "workingDir",
        "boardConfigFile"
    ],
    "properties": {
        "fcrypt": {
            "description": "AOS Service manager crypt configuration",
            "type": "object",
            "required": [
                "CACert"
            ],
            "properties": {
                "CACert": {
                    "description": "CA certificate",
                    "type": "string"
                },
                "tpmDevice": {
                    "type": "string",
                    "description": " Path to TPM device on the system"
                }
            }
        },
        "serviceDiscovery": {
            "description": "Address of service discovery server",
            "type": "string"
        },
        "umController": {
            "description": "UM controller configuration",
            "type": "object",
            "required": [
                "ServerUrl"
            ],
            "properties": {
                "ServerUrl": {
                    "description": "UM controller server url",
                    "type": "string"
                },
                "Cert": {
                    "type": "string",
                    "description": " Path to server certificate"
                },
                "Key": {
                    "type": "string",
                    "description": " Path to key"
                },
                "UmIds": {
                    "type": "array",
                    "items": {
                        "type" : "string",
                        "description": "Id of update manager in the system"
                    }
                }
            }
        },
        "workingDir": {
            "description": "Directory where AOS data will be stored",
            "type": "string"
        },
        "storageDir": {
            "description": "Directory where service storage folders are located",
            "type": "string"
        },
        "updateDir": {
            "description": "Directory where update image are stored",
            "type": "string"
        },
        "layersDir": {
            "description": "Directory where service layers are stored",
            "type": "string"
        },
        "boardConfigFile": {
            "description": "Resource configuration file that contains available system resources for AOS",
            "type": "string"
        },
        "defaultServiceTTLDays": {
            "description": "Specifies how long  to keep service and its data when it is not used",
            "type": "integer",
            "minimum": 0,
            "default": 30
        },
        "iamServer": {
            "description": "Host and port where IAM is located",
            "type": "string"
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
                    "description": "Indicates how many monitoring messages to keep when device in offline",
                    "type": "integer",
                    "minimum": 0,
                    "default": 25
                },
                "bridgeIP": {
                    "description": "Specifies bridge subnet to count as local traffic. Should be set if bridge subnet is not in local segment",
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
                    "description": "Indicates how many alert messages to keep when device in offline",
                    "type": "integer",
                    "minimum": 0,
                    "default": 25
                },
                "filter": {
                    "description": "List of regular expressions which allow to filter out alerts",
                    "type": "array",
                    "default": empty ,
                    "items": {
                        "type": "string"
                    }
                }
            }
        },
        "identifier": {
            "description": "Identifier parameters",
            "type": "object",
            "required": [
                "module"
            ]
        },
        "hostBinds": {
            "description": "The list of host directories/files which should be mounted to the service",
            "type": "array",
            "items": {
                "type": "string"
            }
        },
        "migration": {
            "description": "Database migration config parameters",
            "type": "object",
            "properties": {
                "migrationPath": {
                    "description": "Path of the migration scripts on the rootfs",
                    "type": "string",
                    "default": "/usr/share/aos/servicemanager/migration"
                },
                "mergedMigrationPath": {
                    "description": "Path of the merged migration scripts on rw partition",
                    "type": "string",
                    "default": "/var/aos/servicemanager/mergedMigrationPath"
                }
            }
        },
	"enableDBusServer" {
            "description": "Enables D-Bus server for interaction with Service Manager",
            "type": "boolean",
            "default": false
        }
    }
}
```

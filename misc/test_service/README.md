This is reference service.

Steps to make a custom service:

1. Put service binary and data to home/service folder. This folder will be
   mounted to /home/service folder on the target;
   
2. Update config.json according to service needs. For example, assign startup application and set working directory:

```
	"process": {
		"args": [
			"/home/service/service_app"
		]
	},

	...

	"cwd": "/home/service",

```

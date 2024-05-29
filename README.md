# Init Bot—A Matrix AI assistant Bot

This bot primarily interfaces with [Matrix](https://matrix.org) servers (such as Synapse) and passes on the input
to one or more AI models to handle the request. It takes the response from the AI model, restructures it into a Matrix
message, and replies to the room from where the message originated.
### Primary functions
* Chat assistant
* RAG enabled Document search
* Image generator

The AI backend is configurable in the config.json file and not part of the bot itself. This bot only acts as the
intermediate layer between the AI models and the Matrix server. 

**N.B.!** Currently, it is assumed that the AI backend runs in a safe environment and does not need any authorization.
This is something planned to be fixed in the future.

### Secondary functions
* Keep track of the context per room in a Postgres database.
* Sends typing notifications while the AI is generating responses.

## How to Use

If you have a binary called `bot`, and a file named `config.json` in the same folder as you start the `bot` in,
it is as simple as running `./bot`.

The bot requires the presence of a properly setup configuration file. It can either be in the same folder as you run
the bot from and named exactly "`config.json`", or you can specify the path and name with the "`--config`" command
line argument.

The config file must be JSON formatted and use a specific structure. A `config.example.json` is provided as a
template to use as a base. It contains almost no values and needs to be changed to accurate values for the bot to
function.

The bot relies on a PostgreSQL database as a backend to store both cryptographic information and AI chat context in
order to save context per room.

You can also provide the following command line arguments:

| Argument    | Default Value | Comment                                                                     |
 |-------------|---------------|-----------------------------------------------------------------------------|
| config      | ./config.json | Specify the path (including filename) to the config file                    |
| server      |               | Override the home server address for Matrix from the config file            |
| bot-name    |               | Override the name of the bot from the config file                           |
| username    |               | Override the username for the bot to log into Matrix                        |
| password    |               | Override the password for the bot to log into Matrix                        |
| db-host     |               | Override hostname to the PostgreSQL database                                |
| db-port     | -1            | Override the port number for the PostgreSQL database                        |
| db-username |               | Override the username for the PostgreSQL database                           |
| db-password |               | Override the password for the PostgreSQL database                           |
| db-name     |               | Override the database name in PostgreSQL database                           |
| log-level   |               | Override the log level the bot should use                                   |
| timeout     | -1            | Override the HTTP timeout the bot should use when calling other AI services |

## Build

### Prerequisites

You need to install OLM (Double Ratchet cryptographic library) for this to work.
Look through your package provider or similar for **libolm3**, **libolm-devel**, or similar.
Otherwise, the code cannot run and interact with E2E encrypted rooms in the Matrix server.

### Building

Once that is done, run `go mod tidy` and then `go build -o bot`. You can replace `bot` with any other name that a binary
can have, according to your OS.
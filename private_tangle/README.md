# Private Tangle

This folder contains a Docker-based setup to run your own development private Tangle. The steps to run a private tangle
are:

## Requirements
1. A recent release of Docker enterprise or community edition. You can find installation instructions in the [official Docker documentation](https://docs.docker.com/engine/install/).
2. [Docker Compose CLI plugin](https://docs.docker.com/compose/install/compose-plugin/).

## Steps

1. `./bootstrap.sh` this will bootstrap your own private tangle by creating the genesis snapshot and required files.
   - _**Note:** If you are running this from inside the repository, you should run `./bootstrap.sh build` to re-build the docker images after any updates to the HORNET codebase (e.g. changing files or pulling git changes)_ 
2. Run:
   - `./run.sh` to run COO + 1 additional node.
   - `./run.sh 3` to run COO + 2 additional nodes.
   - `./run.sh 4` to run COO + 3 additional nodes.

3. `./cleanup.sh` to clean up all generated files and start over. 

The nodes will then be reachable under these ports:

- COO:
    - API: http://localhost:14265
    - External Peering: 15600/tcp
    - Dashboard: http://localhost:8081 (username: admin, password: admin)
    - Faucet: http://localhost:8091
    - Prometheus: http://localhost:9311/metrics
- Hornet-2:
    - API: http://localhost:14266
    - External Peering: 15601/tcp
    - Dashboard: http://localhost:8082 (username: admin, password: admin)
    - Prometheus: http://localhost:9312/metrics
- Hornet-3:
    - API: http://localhost:14267
    - External Peering: 15602/tcp
    - Dashboard: http://localhost:8083 (username: admin, password: admin)
    - Prometheus: http://localhost:9313/metrics
- Hornet-4:
    - API: http://localhost:14268
    - External Peering: 15603/tcp
    - Dashboard: http://localhost:8084 (username: admin, password: admin)
    - Prometheus: http://localhost:9314/metrics
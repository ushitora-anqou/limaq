# limaq

A tiny cgroups frontend.

## Usage

See `limaq -help` for details.

```
## Run the program with 2.5 cores of CPU and 1234.5 MB of memory.
$ limaq -cpu 2.5 -mem 1234.5 ./program
```

## Dependency

`cgcreate` and other commands to control cgroups.
In Ubuntu just install `cgroup-tools` package.

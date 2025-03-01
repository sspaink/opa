# Decision Log Plugin

The decision log plugin is responsible for gathering decision events from multiple sources and upload them to a service.
[This plugin is highly configurable](https://www.openpolicyagent.org/docs/latest/configuration/#decision-logs), allowing the user to decide when to upload, drop or proxy a logged event.

Events are uploaded in gzip compressed JSON array's at a user defined interval.

```mermaid
---
title: Event->Upload Flow
---
flowchart LR
    id2([Event Buffer]):::large -. full buffer, size limit .-> id1[(trash)]
    1["Producer 1"] -. event .-> id2
    2["Producer 2"] -. event .-> id2
    3["Producer 3"] -. event .-> id2
    subgraph log [Log Plugin]
        id2 --> package
        subgraph package [Upload Buffer]
            A["[event, event, event, event]"]
        end
    end
    package -. POST .-> service
    classDef large font-size:20pt;
    
```
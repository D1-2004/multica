# dt-fde-multica Pre-Deployment Network Access

## Purpose

`dt-fde-multica` pre-production pods need TCP access to the pre-production
PolarDB PostgreSQL instance so the service can run database migrations at
startup and serve runtime database traffic.

## Current Runtime

- Aone app: `dt-fde-multica`
- Environment: pre-production
- App group: `dt-fde-multica_default_prehost`
- Source site: `na620`
- Source pod IPs: `33.51.241.169`, `33.8.163.3`
- Source VPC: `vpc-8vbhucmd5b2q2fp5aiqqu`

## Target Database

- Product: PolarDB PostgreSQL 17
- Region: `cn-zhangjiakou`
- Cluster: `pc-8vbns4gp0v1q347c4`
- Database: `multica_pre`
- Target VPC: `vpc-8vbwdst411r3bo87dej1m`
- Private primary endpoint:
  `pc-8vbns4gp0v1q347c4.pg.polardb.zhangbei.rds.aliyuncs.com:5432`
- Private cluster endpoint:
  `pc-8vbns4gp0v1q347c4.rwlb.zhangbei.rds.aliyuncs.com:5432`

## Requested Connectivity

Create a PVL cross-cloud channel from the pre-production app group to the
PolarDB VPC:

- Direction: bridge/internal side to Alibaba Cloud
- Connection type: `PVL`
- Network type: `DIRECT`
- Protocol: TCP
- Port: `5432`
- Peak QPS: `10`

The PolarDB whitelist already includes the two pre-production pod IPs. The
remaining blocker is network reachability between the source VPC and target
database VPC.

## Verification

After the channel is ready:

1. Redeploy the Aone pre-production pipeline.
2. Confirm `/home/admin/dt-fde-multica/logs/bootstrap.log` contains
   `migrations completed` and `startup checks passed`.
3. Confirm both pre-production pods are ready.
4. Confirm `https://pre-fde-workbench.dingtalk.com` opens the Multica app.

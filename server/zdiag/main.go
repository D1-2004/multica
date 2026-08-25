package main
import ("context";"fmt";"os";"github.com/jackc/pgx/v5/pgxpool")
func main(){
 ctx:=context.Background()
 pool,err:=pgxpool.New(ctx,os.Getenv("DATABASE_URL")); if err!=nil{fmt.Println(err);os.Exit(1)}; defer pool.Close()
 run:=func(t,sql string){fmt.Println("\n==== "+t+" ====")
  rows,err:=pool.Query(ctx,sql); if err!=nil{fmt.Println("ERR",err);return}; defer rows.Close()
  fds:=rows.FieldDescriptions()
  for rows.Next(){v,_:=rows.Values();l:="";for i:=range v{l+=fmt.Sprintf("%s=%v  ",string(fds[i].Name),v[i])};fmt.Println(l)}}
 // opencode(FC/E2B) runtime 每小时 成功/失败, 看趋势 (近3天)
 run("opencode FC/E2B hourly ok/fail (CST=UTC+8)",`
   SELECT to_char(r.triggered_at,'MM-DD HH24') hr,
          count(*) FILTER (WHERE r.status='completed') ok,
          count(*) FILTER (WHERE r.status='failed') fail
   FROM autopilot_run r JOIN agent_task_queue q ON q.id=r.task_id
   WHERE r.source='schedule' AND q.runtime_id='668b7684-f903-45e1-821f-4e385cfe7e5f'
     AND r.triggered_at>now()-interval '3 days'
   GROUP BY 1 ORDER BY 1 DESC LIMIT 24`)
}

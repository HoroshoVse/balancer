import { useState, useEffect } from "react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Activity, Server, ArrowUpRight, ArrowDownRight } from "lucide-react"
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts'


export default function Dashboard() {
  const [metrics, setMetrics] = useState({
    total_requests: 0,
    healthy_backends: 0,
    total_backends: 0,
    average_latency_ms: 0,
    error_rate_percent: 0,
  })

  const [history, setHistory] = useState<any[]>([])

  useEffect(() => {
    const fetchMetrics = () => {
      const token = localStorage.getItem("token")
      const API_URL = `http://${window.location.hostname}:8080/api/v1/metrics/overview`
      fetch(API_URL, {
        headers: {
          "Authorization": `Bearer ${token}`
        }
      })
        .then(res => {
          if (res.status === 401) {
             localStorage.removeItem("token")
             window.location.href = "/login"
          }
          return res.json()
        })
        .then(data => setMetrics(data))
        .catch(console.error)

      const HISTORY_URL = `http://${window.location.hostname}:8080/api/v1/metrics/history`
      fetch(HISTORY_URL, {
        headers: {
          "Authorization": `Bearer ${token}`
        }
      })
        .then(res => res.json())
        .then(data => setHistory(data))
        .catch(console.error)
    }
    
    fetchMetrics() // Initial fetch
    const interval = setInterval(fetchMetrics, 3000)
    return () => clearInterval(interval)
  }, [])

  return (
    <div className="grid gap-4 md:gap-8">
      <div className="grid gap-4 md:grid-cols-2 md:gap-8 lg:grid-cols-4">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Total Requests</CardTitle>
            <Activity className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{metrics.total_requests.toLocaleString()}</div>
            <p className="text-xs text-muted-foreground">+19% from last hour</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Healthy Backends</CardTitle>
            <Server className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{metrics.healthy_backends} / {metrics.total_backends}</div>
            <p className="text-xs text-green-500 flex items-center">
              <ArrowUpRight className="h-4 w-4 mr-1" />
              All systems operational
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Average Latency</CardTitle>
            <Activity className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{metrics.average_latency_ms.toFixed(0)}ms</div>
            <p className="text-xs text-muted-foreground">Current window</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Error Rate</CardTitle>
            <Activity className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{metrics.error_rate_percent.toFixed(2)}%</div>
            <p className="text-xs text-red-500 flex items-center">
              <ArrowDownRight className="h-4 w-4 mr-1" />
              Slight increase
            </p>
          </CardContent>
        </Card>
      </div>
      
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-7">
        <Card className="col-span-4">
          <CardHeader>
            <CardTitle>Traffic Overview</CardTitle>
          </CardHeader>
          <CardContent className="pl-2">
            <div className="h-[300px] w-full">
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={history}>
                  <CartesianGrid strokeDasharray="3 3" className="stroke-muted" />
                  <XAxis dataKey="timestamp" tickFormatter={(tick) => new Date(tick).toLocaleTimeString()} className="text-xs" />
                  <YAxis yAxisId="left" className="text-xs" />
                  <YAxis yAxisId="right" orientation="right" className="text-xs" />
                  <Tooltip labelFormatter={(label: any) => new Date(label).toLocaleTimeString()} />
                  <Line yAxisId="left" type="monotone" dataKey="rps" stroke="#8884d8" strokeWidth={2} name="RPS" dot={false} />
                  <Line yAxisId="right" type="monotone" dataKey="avg_latency_ms" stroke="#82ca9d" strokeWidth={2} name="Latency (ms)" dot={false} />
                </LineChart>
              </ResponsiveContainer>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

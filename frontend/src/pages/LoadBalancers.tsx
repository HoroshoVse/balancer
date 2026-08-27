import { useEffect, useState } from "react"
import { Card, CardContent } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

export default function LoadBalancers() {
  const [loadBalancers, setLoadBalancers] = useState<any[]>([])

  useEffect(() => {
    const API_URL = `http://${window.location.hostname}:8080/api/v1/load-balancers`
    fetch(API_URL, {
      headers: {
        "Authorization": `Bearer ${localStorage.getItem("token")}`
      }
    })
      .then(res => {
         if (res.status === 401) {
            localStorage.removeItem("token")
            window.location.href = "/login"
         }
         return res.json()
      })
      .then(data => {
        setLoadBalancers(data || [])
      })
      .catch(console.error)
  }, [])

  return (
    <div className="grid gap-4 md:gap-8">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold tracking-tight">Load Balancers</h2>
        <Button>Create Load Balancer</Button>
      </div>
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Protocol</TableHead>
                <TableHead>Address</TableHead>
                <TableHead>Algorithm</TableHead>
                <TableHead>Backends</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>SSL</TableHead>
                <TableHead>Proxy Protocol</TableHead>
                <TableHead>Metrics</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {loadBalancers.map((lb) => (
                <TableRow key={lb.id}>
                  <TableCell className="font-medium">{lb.name}</TableCell>
                  <TableCell>{lb.listen_ip}:{lb.listen_port}</TableCell>
                  <TableCell>{lb.protocol.toUpperCase()}</TableCell>
                  <TableCell>{lb.algorithm}</TableCell>
                  <TableCell>
                    <Badge variant={lb.ssl_enabled ? "default" : "secondary"}>
                      {lb.ssl_enabled ? "Yes" : "No"}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    {lb.proxy_protocol_enabled ? (
                      <Badge variant="outline">PROXYv{lb.proxy_protocol_version}</Badge>
                    ) : (
                      <span className="text-muted-foreground">-</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center space-x-2">
                      <span className="text-xs text-muted-foreground">RPS: {lb.metrics?.total_requests || 0}</span>
                      <span className="text-xs text-muted-foreground">|</span>
                      <span className="text-xs text-muted-foreground">Err: {lb.metrics?.error_rate_percent?.toFixed(1) || 0}%</span>
                      <span className="text-xs text-muted-foreground">|</span>
                      <span className="text-xs text-muted-foreground">Lat: {lb.metrics?.average_latency_ms || 0}ms</span>
                    </div>
                  </TableCell>
                  <TableCell className="text-right">
                    <Button variant="outline" size="sm">Manage</Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}

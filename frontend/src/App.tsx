import { useEffect } from "react"
import { BrowserRouter, Routes, Route, Navigate, Outlet, useLocation } from "react-router-dom"
import { Layout } from "./components/layout/Layout"
import { Toaster } from "./components/ui/sonner"
import { isLoggedIn } from "./lib/api"
import { LBProvider } from "./lib/lbs"
import Builder from "./pages/Builder"
import Dashboard from "./pages/Dashboard"
import LoadBalancers from "./pages/LoadBalancers"
import LoadBalancerDetail from "./pages/LoadBalancerDetail"
import Login from "./pages/Login"
import { Settings } from "./pages/Settings"
import Logs from "./pages/Logs"

/** New page, new scroll position: start at the top. */
function ScrollToTop() {
  const { pathname } = useLocation()
  // Block body: an effect must return nothing or a cleanup function
  useEffect(() => {
    window.scrollTo(0, 0)
  }, [pathname])
  return null
}

function RequireAuth() {
  if (!isLoggedIn()) {
    return <Navigate to="/login" replace />
  }
  return (
    <LBProvider>
      <Outlet />
    </LBProvider>
  )
}

function App() {
  return (
    <BrowserRouter>
      <ScrollToTop />
      <Toaster richColors position="top-right" />
      <Routes>
        <Route path="/login" element={<Login />} />

        <Route element={<RequireAuth />}>
          {/* The builder is a focus-mode screen without the rail */}
          <Route path="/lbs/new" element={<Builder />} />
          <Route path="/lbs/:id/edit" element={<Builder />} />

          <Route element={<Layout />}>
            <Route path="/" element={<Dashboard />} />
            <Route path="/lbs" element={<LoadBalancers />} />
            <Route path="/lbs/:id" element={<LoadBalancerDetail />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="/logs" element={<Logs />} />
          </Route>
        </Route>

        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}

export default App

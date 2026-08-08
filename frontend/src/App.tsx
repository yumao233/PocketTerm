import { BrowserRouter } from "react-router-dom"
import { AuthProvider } from "@/context/AuthContext"
import { Root } from "@/routes/Root"

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Root />
      </AuthProvider>
    </BrowserRouter>
  )
}
# React + Python Demo

This is a minimal `devup` app with:

- `frontend/`: React served by Vite on port `3000`
- `backend/`: Python HTTP server on port `8000`
- `devup.app.yaml`: manifest that starts both services

Run it with:

```bash
devup app up --file examples/react-python-demo/devup.app.yaml
devup app ps --file examples/react-python-demo/devup.app.yaml
devup app logs --file examples/react-python-demo/devup.app.yaml frontend -f
devup app down --file examples/react-python-demo/devup.app.yaml
```

The manifest uses `shadow: true` for both services, so `devup` materializes the
workspace onto native Linux storage inside the VM before launching each service.
Each service also gets a private mount namespace, so the frontend can install
and run directly from `/workspace` without colliding with concurrent `devup run`
probes or sibling services.

The frontend proxies `/api/*` to the backend, so the integration check is:

```bash
devup run --mount "$PWD:/workspace" --workdir /workspace -- bash -lc 'curl -fsS http://127.0.0.1:3000/api/message'
```

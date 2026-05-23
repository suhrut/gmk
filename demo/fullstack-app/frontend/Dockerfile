FROM nginx:1.27-alpine

# Replace the default site config with ours (proxies /api to backend).
COPY nginx.conf /etc/nginx/conf.d/default.conf

# Static site files.
COPY index.html /usr/share/nginx/html/
COPY app.js     /usr/share/nginx/html/
COPY style.css  /usr/share/nginx/html/

EXPOSE 80

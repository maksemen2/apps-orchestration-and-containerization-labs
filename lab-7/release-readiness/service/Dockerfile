FROM python:3.12-slim

WORKDIR /app
COPY service/requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt
COPY service/ /app/
COPY fixtures/ /fixtures/
COPY SKILL.md /SKILL.md
ENV RESULTS_DIR=/results \
    SKILL_PATH=/SKILL.md
EXPOSE 8000
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]

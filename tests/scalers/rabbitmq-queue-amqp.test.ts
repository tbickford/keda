import * as async from 'async'
import * as fs from 'fs'
import * as sh from 'shelljs'
import * as tmp from 'tmp'
import test from 'ava'

const testNamespace = 'rabbitmq-queue-amqp-test'
const rabbitmqNamespace = 'rabbitmq-amqp-test'
const queueName = 'hello'
const username = "test-user"
const password = "test-password"
const vhost = "test-vh"
const connectionString = `amqp://${username}:${password}@rabbitmq.${rabbitmqNamespace}.svc.cluster.local/${vhost}`

test.before(t => {
  // install rabbitmq 
  const rabbitMqTmpFile = tmp.fileSync()
  fs.writeFileSync(rabbitMqTmpFile.name, rabbitmqDeployYaml.replace('{{USERNAME}}', username)
    .replace('{{PASSWORD}}', password)
    .replace('{{VHOST}}', vhost))
  sh.exec(`kubectl create namespace ${rabbitmqNamespace}`)
  t.is(
    0,
    sh.exec(`kubectl apply -f ${rabbitMqTmpFile.name} --namespace ${rabbitmqNamespace}`).code,
    'creating a Rabbit MQ deployment should work.'
  )

  // wait for rabbitmq to load
  for (let i = 0; i < 10; i++) {
    const readyReplicaCount = sh.exec(
      `kubectl get deploy/rabbitmq -n ${rabbitmqNamespace} -o jsonpath='{.status.readyReplicas}'`).stdout
    if (readyReplicaCount != '2') {
      sh.exec('sleep 2s')
    }
  }

  sh.config.silent = true
  // create deployment
  const base64ConStr = Buffer.from(connectionString).toString('base64')
  const tmpFile = tmp.fileSync()
  fs.writeFileSync(tmpFile.name, deployYaml.replace('{{CONNECTION_STRING_BASE64}}', base64ConStr)
    .replace('{{CONNECTION_STRING}}', connectionString)
    .replace('{{QUEUE_NAME}}', queueName))
  sh.exec(`kubectl create namespace ${testNamespace}`)
  t.is(
    0,
    sh.exec(`kubectl apply -f ${tmpFile.name} --namespace ${testNamespace}`).code,
    'creating a deployment should work.'
  )
})

test.serial('Deployment should have 0 replicas on start', t => {
  const replicaCount = sh.exec(
    `kubectl get deployment.apps/test-deployment --namespace ${testNamespace} -o jsonpath="{.spec.replicas}"`
  ).stdout
  t.is(replicaCount, '0', 'replica count should start out as 0')
})

test.serial('Deployment should scale to 4 with 500 messages on the queue then back to 0', t => {
  const messageCount = 500
  // publish messages
  const tmpFile = tmp.fileSync()
  fs.writeFileSync(tmpFile.name, publishYaml.replace('{{CONNECTION_STRING}}', connectionString)
    .replace('{{MESSAGE_COUNT}}', messageCount.toString()))
  t.is(
    0,
    sh.exec(`kubectl apply -f ${tmpFile.name} --namespace ${testNamespace}`).code,
    'publishing job should apply.'
  )

  // wait for the publishing job to complete
  for (let i = 0; i < 20; i++) {
    const succeeded = sh.exec(`kubectl get job rabbitmq-publish --namespace ${testNamespace} -o jsonpath='{.status.succeeded}'`).stdout
    if (succeeded == '1') {
      break
    }
    sh.exec('sleep 1s')
  }

  // with messages published, the consumer deployment should start receiving the messages
  let replicaCount = '0'
  for (let i = 0; i < 10 && replicaCount !== '4'; i++) {
    replicaCount = sh.exec(
      `kubectl get deployment.apps/test-deployment --namespace ${testNamespace} -o jsonpath="{.spec.replicas}"`
    ).stdout
    t.log('replica count is:' + replicaCount)
    if (replicaCount !== '4') {
      sh.exec('sleep 5s')
    }
  }

  t.is('4', replicaCount, 'Replica count should be 4 after 10 seconds')

  for (let i = 0; i < 50 && replicaCount !== '0'; i++) {
    replicaCount = sh.exec(
      `kubectl get deployment.apps/test-deployment --namespace ${testNamespace} -o jsonpath="{.spec.replicas}"`
    ).stdout
    if (replicaCount !== '0') {
      sh.exec('sleep 5s')
    }
  }

  t.is('0', replicaCount, 'Replica count should be 0 after 3 minutes')
})

test.after.always.cb('clean up rabbitmq-queue deployment', t => {
  const resources = [
    'secret/test-secrets',
    'deployment.apps/test-deployment',
    'scaledobject.keda.k8s.io/test-scaledobject',
  ]

  for (const resource of resources) {
    sh.exec(`kubectl delete ${resource} --namespace ${testNamespace}`)
  }
  sh.exec(`kubectl delete namespace ${testNamespace}`)

 
  // remove rabbitmq 
  sh.exec(`kubectl delete -n ${rabbitmqNamespace} -f scalers/rabbitmq-deployment.yaml`)
  sh.exec(`kubectl delete namespace ${rabbitmqNamespace}`)
  t.end()
})


const publishYaml = `apiVersion: batch/v1
kind: Job
metadata:
  name: rabbitmq-publish
spec:
  template:
    spec:
      containers:
      - name: rabbitmq-client
        image: jeffhollan/rabbitmq-client:dev
        imagePullPolicy: Always
        command: ["send",  "{{CONNECTION_STRING}}", "{{MESSAGE_COUNT}}"]
      restartPolicy: Never`

const deployYaml = `apiVersion: v1
kind: Secret
metadata:
  name: test-secrets
data:
  RabbitMqHost: {{CONNECTION_STRING_BASE64}}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deployment
  labels:
    app: test-deployment
spec:
  replicas: 0 
  selector:
    matchLabels:
      app: test-deployment
  template:
    metadata:
      labels:
        app: test-deployment
    spec:
      containers:
      - name: rabbitmq-consumer
        image: jeffhollan/rabbitmq-client:dev
        imagePullPolicy: Always
        command:
          - receive
        args:
          - '{{CONNECTION_STRING}}'
        envFrom:
        - secretRef:
            name: test-secrets
---
apiVersion: keda.k8s.io/v1alpha1
kind: ScaledObject
metadata:
  name: test-scaledobject
  labels:
    deploymentName: test-deployment
spec:
  scaleTargetRef:
    deploymentName: test-deployment
  pollingInterval: 5
  cooldownPeriod: 10
  minReplicaCount: 0
  maxReplicaCount: 4
  triggers:
  - type: rabbitmq
    metadata:
      queueName: {{QUEUE_NAME}}
      host: RabbitMqHost
      queueLength: '50'`

const rabbitmqDeployYaml = `apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app: rabbitmq
  name: rabbitmq
spec:
  replicas: 1
  selector:
    matchLabels:
      app: rabbitmq
  template:
    metadata:
      labels:
        app: rabbitmq
    spec:
      containers:
      - image: rabbitmq:3-management
        name: rabbitmq
        env:
        - name: RABBITMQ_DEFAULT_USER 
          value: "{{USERNAME}}"
        - name: RABBITMQ_DEFAULT_PASS 
          value: "{{PASSWORD}}"
        - name: RABBITMQ_DEFAULT_VHOST
          value: "{{VHOST}}"
---
apiVersion: v1
kind: Service
metadata:
  labels:
    app: rabbitmq
  name: rabbitmq
spec:
  ports:
  - name: amqp
    port: 5672
    protocol: TCP
    targetPort: 5672
  - name: http
    port: 15672
    protocol: TCP
    targetPort: 15672
  selector:
    app: rabbitmq`